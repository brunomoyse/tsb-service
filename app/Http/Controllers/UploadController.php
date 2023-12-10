<?php

namespace App\Http\Controllers;

use App\Models\Product;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Http\UploadedFile;
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
        $timeSuffix = '-'.time();
        $baseFileName = $product->slug.$timeSuffix; // Name without extension

        $formats = ['avif', 'webp', 'png'];
        foreach ($formats as $format) {
            $fileName = $baseFileName.'.'.$format;
            $originalPath = public_path('images').'/'.$fileName;

            // Convert and save the original image in different formats
            $imagick = new Imagick();
            $imagick->readImageBlob(file_get_contents($originalImage->getPathname()));
            $imagick->setImageFormat($format);
            $imagick->writeImage($originalPath);

            // Create and save the thumbnail in different formats
            $thumbnailPath = public_path('images/thumbnails').'/'.$fileName;
            $imagick->thumbnailImage(200, 0); // 200px width, maintain aspect ratio
            $imagick->writeImage($thumbnailPath);
        }

        // Save only one record without the extension
        $product->attachments()->create([
            'path' => $baseFileName,
        ]);

        return response()->json([
            'success' => true,
            'message' => 'Image and thumbnails saved in multiple formats, one attachment record created.',
        ]);
    }

}
