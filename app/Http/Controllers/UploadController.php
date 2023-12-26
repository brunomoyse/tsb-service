<?php

namespace App\Http\Controllers;

use App\Models\Product;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Http\UploadedFile;
use Illuminate\Support\Facades\App;
use Imagick;
use ImagickException;

class UploadController extends Controller
{
    /**
     * @throws ImagickException
     */
    public function uploadImage(Request $request): JsonResponse
    {
        $request->validate([
            'image' => 'required|image|mimes:jpeg,png,jpg,gif|max:2048',
            'product_id' => 'required|uuid|exists:products,id',
        ]);

        /** @var Product $product */
        $product = Product::query()->findOrFail($request->product_id);

        /** @var UploadedFile $originalImage */
        $originalImage = $request->file('image');
        //$timeSuffix = '-'.time();
        $timeSuffix = '';
        $baseFileName = $product->slug.$timeSuffix; // Name without extension

        $formats = ['avif', 'webp', 'png'];
        foreach ($formats as $format) {
            $fileName = $baseFileName.'.'.$format;

            $s3 = App::make('aws')->createClient('s3');

            // Create and save the normal size image using Imagick
            $normalSize = new Imagick();
            /** @var string $image */
            $image = file_get_contents($originalImage->getPathname());
            $normalSize->readImageBlob($image);
            $normalSize->thumbnailImage(600, 0);
            $normalSize->setImageFormat($format);

            // Save the thumbnail to a temporary file
            $normalSizePath = tempnam(sys_get_temp_dir(), 'normal-size').'.'.$format;
            $normalSize->writeImage($normalSizePath);

            /** @phpstan-ignore-next-line */
            $s3->putObject([
                'Bucket' => 'tsb-storage',
                'Key' => 'images/'.$fileName,
                'SourceFile' => $normalSizePath,
            ]);

            // Create and save the thumbnail using Imagick
            $thumbnail = new Imagick();
            /** @var string $image */
            $image = file_get_contents($originalImage->getPathname());
            $thumbnail->readImageBlob($image);
            $thumbnail->thumbnailImage(350, 0); // 300px width, and maintain aspect ratio
            $thumbnail->setImageFormat($format);

            // Save the thumbnail to a temporary file
            $tempThumbnailPath = tempnam(sys_get_temp_dir(), 'thumb').'.'.$format;
            $thumbnail->writeImage($tempThumbnailPath);

            // Save the thumbnail to S3
            /** @phpstan-ignore-next-line */
            $s3->putObject([
                'Bucket' => 'tsb-storage',
                'Key' => 'images/thumbnails/'.$fileName,
                'SourceFile' => $tempThumbnailPath,
            ]);

            // Delete the temporary thumbnail file
            unlink($normalSizePath);
            unlink($tempThumbnailPath);
        }

        // Save only one record without the extension
        $product->attachments()->create([
            'path' => $baseFileName,
            'preview' => true,
        ]);

        return response()->json([
            'success' => true,
            'message' => 'Image and thumbnails saved in multiple formats, one attachment record created.',
        ]);
    }
}
